package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
)

// fetchGoogleEmail resolves the account email for an OAuth access token via the
// Google userinfo endpoint. Lets the plugin self-identify when GMAIL_EMAIL is
// unset, so an OAuth connection alone is enough (no separate email config).
func fetchGoogleEmail(token string) (string, error) {
	req, err := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo request failed (%d)", resp.StatusCode)
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	return info.Email, nil
}

// xoauth2String builds the SASL XOAUTH2 initial client response for Gmail.
func xoauth2String(email, token string) string {
	return fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", email, token)
}

// imapXOAuth2 implements github.com/emersion/go-sasl Client for IMAP AUTHENTICATE
// XOAUTH2. (Implemented inline so we don't add a dependency.)
type imapXOAuth2 struct {
	email string
	token string
}

func (a *imapXOAuth2) Start() (mech string, ir []byte, err error) {
	return "XOAUTH2", []byte(xoauth2String(a.email, a.token)), nil
}

func (a *imapXOAuth2) Next(challenge []byte) ([]byte, error) {
	// A challenge here means the server rejected the token (it carries a base64
	// JSON error). Respond empty so the exchange terminates with the server error.
	return []byte(""), nil
}

// smtpXOAuth2 implements net/smtp.Auth for SMTP AUTH XOAUTH2.
type smtpXOAuth2 struct {
	email string
	token string
}

func (a *smtpXOAuth2) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "XOAUTH2", []byte(xoauth2String(a.email, a.token)), nil
}

func (a *smtpXOAuth2) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return []byte(""), nil
	}
	return nil, nil
}

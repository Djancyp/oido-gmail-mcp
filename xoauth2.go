package main

import (
	"fmt"
	"net/smtp"
)

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

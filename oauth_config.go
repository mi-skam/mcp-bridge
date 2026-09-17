package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
)

// OAuthOptions accepts the boolean or object form used by zot-mcp.
// Explicit false disables OAuth; omission preserves existing automatic behavior.
type OAuthOptions struct {
	Disabled     bool   `json:"-"`
	RedirectURI  string `json:"redirectUri,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

func (o *OAuthOptions) UnmarshalJSON(data []byte) error {
	*o = OAuthOptions{}
	switch string(bytes.TrimSpace(data)) {
	case "true":
		return nil
	case "false":
		o.Disabled = true
		return nil
	}
	type fields OAuthOptions
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode((*fields)(o)); err != nil {
		return errors.New("oauth must be a boolean or an object with redirectUri, clientId, clientSecret and scope")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid oauth configuration")
	}
	return nil
}

func (o OAuthOptions) MarshalJSON() ([]byte, error) {
	if o.Disabled {
		return []byte("false"), nil
	}
	type fields OAuthOptions
	return json.Marshal(fields(o))
}

func (o OAuthOptions) validate() error {
	if o.ClientSecret != "" && o.ClientID == "" {
		return errors.New("oauth.clientSecret requires clientId")
	}
	if o.RedirectURI != "" {
		u, err := url.Parse(o.RedirectURI)
		if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("oauth.redirectUri must be an HTTP loopback IP URL with an explicit port and no query or fragment")
		}
	}
	return nil
}

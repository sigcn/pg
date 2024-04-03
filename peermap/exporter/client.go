package exporter

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/rkonfj/peerguard/peermap/exporter/auth"
)

type peermapTransport struct {
	authenticator *auth.Authenticator
	t             http.RoundTripper
}

func (c *peermapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := c.authenticator.GenerateToken(auth.Instruction{ExpiredAt: time.Now().Add(10 * time.Second).Unix()})
	if err != nil {
		return nil, err
	}
	req.Header.Add("X-Token", token)
	return c.t.RoundTrip(req)
}

type Client struct {
	c *http.Client
}

func NewClient(secretKey string) (*Client, error) {
	return &Client{
		c: &http.Client{
			Transport: &peermapTransport{
				authenticator: auth.New(secretKey),
				t:             http.DefaultTransport,
			},
		},
	}, nil
}

func (c *Client) Networks(peermapURL string) ([]NetworkHead, error) {
	peermap, err := url.Parse(peermapURL)
	if err != nil {
		return nil, err
	}
	peermap.Path = path.Join(peermap.Path, "/networks")
	resp, err := c.c.Get(peermap.String())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("got unexpected status:" + resp.Status)
	}
	defer resp.Body.Close()
	var heads []NetworkHead
	json.NewDecoder(resp.Body).Decode(&heads)
	return heads, nil
}

func (c *Client) Peers(peermapURL string) ([]Network, error) {
	peermap, err := url.Parse(peermapURL)
	if err != nil {
		return nil, err
	}
	peermap.Path = path.Join(peermap.Path, "/peers")
	resp, err := c.c.Get(peermap.String())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("got unexpected status:" + resp.Status)
	}
	defer resp.Body.Close()
	var networks []Network
	json.NewDecoder(resp.Body).Decode(&networks)
	return networks, nil
}

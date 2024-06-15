package exporter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	peermapURL *url.URL
	c          *http.Client
}

func NewClient(peermapURL, secretKey string) (*Client, error) {
	pURL, err := url.Parse(peermapURL)
	if err != nil {
		return nil, fmt.Errorf("invalid peermap url: %w", err)
	}
	return &Client{
		peermapURL: pURL,
		c: &http.Client{
			Transport: &peermapTransport{
				authenticator: auth.New(secretKey),
				t:             http.DefaultTransport,
			},
		},
	}, nil
}

func (c *Client) Networks() ([]NetworkHead, error) {
	peermap := *c.peermapURL
	peermap.Path = path.Join(peermap.Path, "/networks")
	resp, err := c.c.Get(peermap.String())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("got unexpected status: " + resp.Status)
	}
	defer resp.Body.Close()
	var heads []NetworkHead
	json.NewDecoder(resp.Body).Decode(&heads)
	return heads, nil
}

func (c *Client) Peers() ([]Network, error) {
	peermap := *c.peermapURL
	peermap.Path = path.Join(peermap.Path, "/peers")
	resp, err := c.c.Get(peermap.String())
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("got unexpected status: " + resp.Status)
	}
	defer resp.Body.Close()
	var networks []Network
	json.NewDecoder(resp.Body).Decode(&networks)
	return networks, nil
}

func (c *Client) PutNetworkMeta(network string, request PutNetworkMetaRequest) error {
	peermap := *c.peermapURL
	peermap.Path = path.Join(peermap.Path, fmt.Sprintf("/network/%s/meta", network))
	b, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	r, err := http.NewRequest(http.MethodPut, peermap.String(), bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := c.c.Do(r)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("got unexpected status: " + resp.Status)
	}
	return nil
}

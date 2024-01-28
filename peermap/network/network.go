package network

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/rkonfj/peerguard/peermap/oidc"
	"storj.io/common/base58"
)

var (
	client = &http.Client{}
)

type JoinIntent struct {
	state   string
	authURL string
	peermap *url.URL
}

func (intent *JoinIntent) AuthURL() string {
	return fmt.Sprintf("%s?state=%s", intent.authURL, intent.state)
}

func (intent *JoinIntent) Wait(ctx context.Context) (*oidc.NetworkSecret, error) {
	resp, err := client.Get(fmt.Sprintf("https://%s/network/token?state=%s", intent.peermap.Host, intent.state))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wait token error: %s", resp.Status)
	}

	defer resp.Body.Close()

	var joined oidc.NetworkSecret
	if err := json.NewDecoder(resp.Body).Decode(&joined); err != nil {
		return nil, err
	}
	return &joined, nil
}

func JoinOIDC(peermap, oidcProvider string) (*JoinIntent, error) {
	peermapURL, err := url.Parse(peermap)
	if err != nil {
		return nil, err
	}
	state := make([]byte, 12)
	rand.Read(state)
	return &JoinIntent{
		state:   base58.Encode(state),
		authURL: fmt.Sprintf("https://%s/oidc/%s", peermapURL.Host, oidcProvider),
		peermap: peermapURL,
	}, nil
}

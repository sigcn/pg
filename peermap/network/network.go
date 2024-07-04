package network

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"

	"github.com/rkonfj/peerguard/peer"
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

func (intent *JoinIntent) Wait(ctx context.Context) (joined peer.NetworkSecret, err error) {
	tokenURL := fmt.Sprintf("https://%s/oidc/secret?state=%s", intent.peermap.Host, intent.state)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}

	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("wait token error: %s", resp.Status)
		return
	}

	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&joined)
	return
}

func JoinOIDC(oidcProvider, peermap string) (*JoinIntent, error) {
	peermapURL, err := url.Parse(peermap)
	if err != nil {
		return nil, err
	}
	joinPath := "/oidc"
	if oidcProvider != "" {
		joinPath = path.Join(joinPath, oidcProvider)
	}
	state := make([]byte, 12)
	rand.Read(state)
	return &JoinIntent{
		state:   base58.Encode(state),
		authURL: fmt.Sprintf("https://%s%s", peermapURL.Host, joinPath),
		peermap: peermapURL,
	}, nil
}

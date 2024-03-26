package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/rkonfj/peerguard/peer"
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

func (intent *JoinIntent) Wait(ctx context.Context) (peer.NetworkSecret, error) {
	resp, err := client.Get(fmt.Sprintf("https://%s/network/token?state=%s", intent.peermap.Host, intent.state))
	if err != nil {
		return peer.NetworkSecret{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return peer.NetworkSecret{}, fmt.Errorf("wait token error: %s", resp.Status)
	}

	defer resp.Body.Close()

	var joined peer.NetworkSecret
	if err := json.NewDecoder(resp.Body).Decode(&joined); err != nil {
		return peer.NetworkSecret{}, err
	}
	return joined, nil
}

func JoinOIDC(oidcProvider string, cluster peer.PeermapCluster) (*JoinIntent, error) {
	if len(cluster) == 0 {
		return nil, errors.New("no peermap server avaiable")
	}
	peermapURL, err := url.Parse(cluster[0])
	if err != nil {
		return nil, err
	}
	state := make([]byte, 12)
	rand.Read(state)
	return &JoinIntent{
		state:   hex.EncodeToString(state),
		authURL: fmt.Sprintf("https://%s/oidc/%s", peermapURL.Host, oidcProvider),
		peermap: peermapURL,
	}, nil
}

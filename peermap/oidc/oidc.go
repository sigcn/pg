package oidc

import (
	"context"
	"errors"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	ProviderGoogle = "google"
	ProviderGithub = "github"
)

var providers = make(map[string]*OIDCProvider)

type OIDCProviderConfig struct {
	Name         string   `yaml:"name"`
	Issuer       string   `yaml:"issuer"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	RedirectURL  string   `yaml:"redirect_url"`
	Scopes       []string `yaml:"scopes"`
	AuthURL      string   `yaml:"auth_url"`
	TokenURL     string   `yaml:"token_url"`
	UserInfoURL  string   `yaml:"user_info_url"`
}

type OIDCProvider struct {
	standardOIDC bool
	privoder     *oidc.Provider
	oAuthConfig  *oauth2.Config
}

func (p *OIDCProvider) UserInfo(code string) (email string, extra map[string]any, err error) {
	exchangeCtx, exchangeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer exchangeCancel()
	token, err := p.oAuthConfig.Exchange(exchangeCtx, code)
	if err != nil {
		return
	}
	userInfoCtx, userInfoCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer userInfoCancel()
	userInfo, err := p.privoder.UserInfo(userInfoCtx, p.oAuthConfig.TokenSource(context.Background(), token))
	if err != nil {
		return
	}
	if p.standardOIDC && !userInfo.EmailVerified {
		err = errors.New("email is not verified")
		return
	}
	email = userInfo.Email
	err = userInfo.Claims(&extra)
	return
}

func AddProvider(oidcProviderConfig OIDCProviderConfig) (err error) {
	providerCtx, providerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer providerCancel()
	var (
		provider     *oidc.Provider
		standardOIDC bool
	)
	if len(oidcProviderConfig.Issuer) > 0 {
		provider, err = oidc.NewProvider(providerCtx, oidcProviderConfig.Issuer)
		if err != nil {
			return
		}
		standardOIDC = true
	} else {
		providerConfig := oidc.ProviderConfig{
			AuthURL:     oidcProviderConfig.AuthURL,
			TokenURL:    oidcProviderConfig.TokenURL,
			UserInfoURL: oidcProviderConfig.UserInfoURL,
		}
		provider = providerConfig.NewProvider(providerCtx)
	}

	providers[oidcProviderConfig.Name] = &OIDCProvider{
		standardOIDC: standardOIDC,
		privoder:     provider,
		oAuthConfig: &oauth2.Config{
			ClientID:     oidcProviderConfig.ClientID,
			ClientSecret: oidcProviderConfig.ClientSecret,
			RedirectURL:  oidcProviderConfig.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       oidcProviderConfig.Scopes,
		},
	}
	return
}

func Provider(providerName string) (*OIDCProvider, bool) {
	provider, ok := providers[providerName]
	return provider, ok
}

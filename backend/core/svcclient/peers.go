// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"errors"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
)

var ErrPeerNotConfigured = errors.New("peer not configured")

type MissingError struct{ Key string }

func (e MissingError) Error() string        { return e.Key + ": " + ErrPeerNotConfigured.Error() }
func (e MissingError) Is(target error) bool { return target == ErrPeerNotConfigured }

type Peers struct {
	infra config.InfrastructureConfiguration
	area  string
	opts  []Option
}

// NewPeers accepts any non-blank functional area; service ceilings are checked by the repository guard.
func NewPeers(infra config.InfrastructureConfiguration, area string) *Peers {
	if strings.TrimSpace(area) == "" {
		panic("svcclient: blank functional area")
	}
	return &Peers{infra: infra, area: area}
}

// Area is the calling service's token subject and log identity.
func (p *Peers) Area() string { return p.area }

func (p *Peers) Ready() error {
	if err := p.mintReady(); err != nil {
		return err
	}
	if !p.infra.UserManagement.Endpoint().Configured() {
		return MissingError{Key: "infrastructure.userManagement"}
	}
	return nil
}

func (p *Peers) With(opts ...Option) *Peers {
	copy := *p
	copy.opts = append(append([]Option(nil), p.opts...), opts...)
	return &copy
}

// Client preserves New's internal mint address and creates an isolated token cache.
func (p *Peers) Client(authorities ...auth.Authority) *Client {
	grants := make([]string, len(authorities))
	for i, a := range authorities {
		grants[i] = string(a)
	}
	return New(p.infra.UserManagement, p.infra.ServiceAuth.Secret, p.area, grants, p.opts...)
}

type Seam struct {
	Client *Client
	URL    string
}

func (p *Peers) Seam(target config.ServiceEndpoint, authorities ...auth.Authority) (Seam, error) {
	if err := p.Ready(); err != nil {
		return Seam{}, err
	}
	url, err := p.URL(target)
	if err != nil {
		return Seam{}, err
	}
	return Seam{Client: p.Client(authorities...), URL: url}, nil
}
func (p *Peers) UserManagement(authorities ...auth.Authority) (Seam, error) {
	return p.Seam(p.infra.UserManagement.Endpoint(), authorities...)
}

// URL also supports existing sites that construct a client before the mint is ready.
func (p *Peers) URL(target config.ServiceEndpoint) (string, error) {
	if err := p.mintReady(); err != nil {
		return "", err
	}
	if !target.Configured() {
		return "", MissingError{Key: target.Key}
	}
	return target.GraphQLURL(), nil
}

// mintReady says whether this area holds what its mint needs. For an area in auth.AreaBoundCallers
// that is its OWN mounted credential, and only that: a shared secret in its configuration does not
// make it ready, and is never used (New discards it for an area-bound subject).
func (p *Peers) mintReady() error {
	if auth.IsAreaBoundCaller(p.area) {
		path := auth.AreaMintCredentialFile(areaCredentialDir, p.area)
		if _, err := readAreaCredential(path); err != nil {
			return MissingError{Key: path}
		}
		return nil
	}
	if p.infra.ServiceAuth.Secret == "" {
		return MissingError{Key: "infrastructure.serviceAuth.secret"}
	}
	return nil
}

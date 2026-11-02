// Package sso talks to AWS IAM Identity Center: the OIDC login flows (browser
// authorization code + PKCE, and device code), an AWS CLI v2 compatible token
// cache, the SSO portal API (accounts, roles, role credentials) and account
// discovery.
//
// Error classification is central to CredOrbit's safety: only an explicit
// ForbiddenException maps to model.ErrNoAccess (revoked access). Throttling,
// 5xx and network/TLS failures map to model.ErrTransient and must never be taken
// as revocation.
package sso

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/ratelimit"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awssso "github.com/aws/aws-sdk-go-v2/service/sso"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/PiniPP/credorbit/internal/model"
)

// Client is the SSO portal API used by the rest of CredOrbit.
type Client interface {
	// ListAccounts returns every account assigned to the user (all pages).
	ListAccounts(ctx context.Context, accessToken string) ([]model.Account, error)
	// ListAccountRoles returns every role name assigned in accountID (all pages).
	ListAccountRoles(ctx context.Context, accessToken, accountID string) ([]string, error)
	// GetRoleCredentials returns temporary credentials for accountID/role.
	GetRoleCredentials(ctx context.Context, accessToken, accountID, role string) (model.Credentials, error)
}

const (
	maxAttempts       = 6
	defaultMaxBackoff = 20 * time.Second
)

// NewClient returns an SDK-backed portal client for region using hc (nil → SDK
// default). Requests are unsigned (the bearer token authenticates), retried by
// the SDK standard retryer (6 attempts, exponential backoff with jitter) and
// errors are mapped to the model.Err* classes.
func NewClient(region string, hc *http.Client) Client {
	return newPortal(region, hc, "", defaultMaxBackoff)
}

// NewClientWithEndpoint is NewClient with an explicit portal endpoint (e.g. an
// httptest server, a VPC endpoint or a FIPS endpoint). "" uses the default
// https://portal.sso.<region>.amazonaws.com resolution.
func NewClientWithEndpoint(region string, hc *http.Client, endpoint string) Client {
	return newPortal(region, hc, endpoint, defaultMaxBackoff)
}

// NewClientWithBackoff is NewClientWithEndpoint with an explicit maximum retry
// backoff (tests use a few milliseconds so retried 5xx responses stay fast).
func NewClientWithBackoff(region string, hc *http.Client, endpoint string, maxBackoff time.Duration) Client {
	if maxBackoff <= 0 {
		maxBackoff = defaultMaxBackoff
	}
	return newPortal(region, hc, endpoint, maxBackoff)
}

type portal struct {
	api *awssso.Client
}

func newPortal(region string, hc *http.Client, endpoint string, maxBackoff time.Duration) *portal {
	o := awssso.Options{
		Region:      region,
		Credentials: aws.AnonymousCredentials{},
		Retryer:     newRetryer(maxBackoff),
	}
	if hc != nil {
		o.HTTPClient = hc
	}
	if endpoint != "" {
		o.BaseEndpoint = aws.String(strings.TrimRight(endpoint, "/"))
	}
	return &portal{api: awssso.New(o)}
}

// newRetryer is the SDK standard retryer with a fixed attempt budget and no
// client-side retry quota: discovery fans out many calls in parallel and a
// drained token bucket would turn recoverable throttling into hard failures.
func newRetryer(maxBackoff time.Duration) aws.Retryer {
	return retry.NewStandard(func(o *retry.StandardOptions) {
		o.MaxAttempts = maxAttempts
		o.MaxBackoff = maxBackoff
		o.Backoff = retry.NewExponentialJitterBackoff(maxBackoff)
		o.RateLimiter = ratelimit.None
	})
}

func (p *portal) ListAccounts(ctx context.Context, accessToken string) ([]model.Account, error) {
	pg := awssso.NewListAccountsPaginator(p.api, &awssso.ListAccountsInput{AccessToken: aws.String(accessToken)},
		func(o *awssso.ListAccountsPaginatorOptions) { o.StopOnDuplicateToken = true })
	var out []model.Account
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, mapErr(ctx, "ListAccounts", err)
		}
		for _, a := range page.AccountList {
			out = append(out, model.Account{
				ID:    aws.ToString(a.AccountId),
				Name:  aws.ToString(a.AccountName),
				Email: aws.ToString(a.EmailAddress),
			})
		}
	}
	return out, nil
}

func (p *portal) ListAccountRoles(ctx context.Context, accessToken, accountID string) ([]string, error) {
	pg := awssso.NewListAccountRolesPaginator(p.api, &awssso.ListAccountRolesInput{
		AccessToken: aws.String(accessToken),
		AccountId:   aws.String(accountID),
	}, func(o *awssso.ListAccountRolesPaginatorOptions) { o.StopOnDuplicateToken = true })
	seen := map[string]bool{}
	out := []string{}
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, mapErr(ctx, "ListAccountRoles", err)
		}
		for _, r := range page.RoleList {
			if n := aws.ToString(r.RoleName); n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (p *portal) GetRoleCredentials(ctx context.Context, accessToken, accountID, role string) (model.Credentials, error) {
	out, err := p.api.GetRoleCredentials(ctx, &awssso.GetRoleCredentialsInput{
		AccessToken: aws.String(accessToken),
		AccountId:   aws.String(accountID),
		RoleName:    aws.String(role),
	})
	if err != nil {
		return model.Credentials{}, mapErr(ctx, "GetRoleCredentials", err)
	}
	rc := out.RoleCredentials
	if rc == nil || aws.ToString(rc.AccessKeyId) == "" || aws.ToString(rc.SecretAccessKey) == "" || aws.ToString(rc.SessionToken) == "" {
		return model.Credentials{}, fmt.Errorf("sso GetRoleCredentials: incomplete credentials in response: %w", model.ErrTransient)
	}
	c := model.Credentials{
		AccessKeyID:     aws.ToString(rc.AccessKeyId),
		SecretAccessKey: aws.ToString(rc.SecretAccessKey),
		SessionToken:    aws.ToString(rc.SessionToken),
	}
	if rc.Expiration > 0 {
		c.Expiration = time.UnixMilli(rc.Expiration).UTC()
	}
	return c, nil
}

// logout invalidates the access token server-side (best effort).
func (p *portal) logout(ctx context.Context, accessToken string) error {
	_, err := p.api.Logout(ctx, &awssso.LogoutInput{AccessToken: aws.String(accessToken)})
	if err != nil {
		return mapErr(ctx, "Logout", err)
	}
	return nil
}

// mapErr wraps an SDK error with its model error class. The SDK error text
// carries operation, status, request ID and the service message — never request
// headers, so the bearer token cannot leak through it.
func mapErr(ctx context.Context, op string, err error) error {
	if class := classify(ctx, err); class != nil {
		return fmt.Errorf("sso %s: %w: %w", op, class, err)
	}
	return fmt.Errorf("sso %s: %w", op, err)
}

// classify returns the model error class for err, or nil when it has none
// (including context cancellation, which callers detect with errors.Is).
func classify(ctx context.Context, err error) error {
	if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return nil
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "ForbiddenException":
			return model.ErrNoAccess
		case "UnauthorizedException", "InvalidGrantException", "ExpiredTokenException",
			"InvalidClientException", "UnauthorizedClientException":
			return model.ErrUnauthorized
		case "ResourceNotFoundException":
			return model.ErrNotFound
		case "TooManyRequestsException", "ThrottlingException", "Throttling", "SlowDownException",
			"InternalServerException", "ServiceUnavailableException", "RequestTimeout", "RequestTimeoutException":
			return model.ErrTransient
		}
	}
	var re *smithyhttp.ResponseError
	if errors.As(err, &re) {
		if re.HTTPStatusCode() >= 500 || re.HTTPStatusCode() == http.StatusTooManyRequests {
			return model.ErrTransient
		}
		if re.HTTPStatusCode() == http.StatusUnauthorized {
			return model.ErrUnauthorized
		}
		// A 403 without the ForbiddenException code typically comes from a
		// proxy or WAF; it is NOT treated as revocation.
	}
	var (
		se *smithyhttp.RequestSendError
		ne net.Error
	)
	if errors.As(err, &se) || errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) {
		return model.ErrTransient
	}
	return nil
}

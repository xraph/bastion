package gateway

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/xraph/forge"

	"github.com/xraph/bastion"
)

// adminRouteOptions returns the route options every admin API route is
// registered with: the AdminToken check, then any AdminMiddleware. With
// neither configured it returns nothing and, unless the operator set
// AllowUnauthenticatedAdmin, logs a warning, because the admin API can
// create, change and delete routes.
func adminRouteOptions(gw *bastion.Gateway) []forge.RouteOption {
	d := gw.Config().Dashboard

	if !d.AdminAuthenticated() {
		if !d.AllowUnauthenticatedAdmin {
			gw.Logger().Warn("bastion: admin API registered without authentication; anyone who can reach it can change routes. "+
				"Set an admin token (WithAdminToken) or admin middleware (WithAdminMiddleware), "+
				"or set WithAllowUnauthenticatedAdmin(true) if it is protected some other way",
				forge.F("admin_base", d.BasePath+"/api"),
			)
		}

		return nil
	}

	mws := make([]forge.Middleware, 0, len(d.AdminMiddleware)+1)
	if d.AdminToken != "" {
		mws = append(mws, requireBearer(d.AdminToken))
	}

	mws = append(mws, d.AdminMiddleware...)

	return []forge.RouteOption{forge.WithMiddleware(mws...)}
}

// requireBearer rejects a request unless it carries
// "Authorization: Bearer <token>". The comparison is constant time.
func requireBearer(token string) forge.Middleware {
	want := []byte(token)

	return func(next forge.Handler) forge.Handler {
		return func(ctx forge.Context) error {
			if subtle.ConstantTimeCompare([]byte(bearer(ctx.Request())), want) != 1 {
				ctx.Response().Header().Set("WWW-Authenticate", `Bearer realm="bastion-admin"`)

				return ctx.JSON(http.StatusUnauthorized, map[string]string{"error": "admin API requires a valid bearer token"})
			}

			return next(ctx)
		}
	}
}

// bearer returns the token from an "Authorization: Bearer" header, or ""
// when there isn't one. The scheme name is matched case-insensitively.
func bearer(r *http.Request) string {
	const scheme = "bearer "

	h := r.Header.Get("Authorization")
	if len(h) <= len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return ""
	}

	return strings.TrimSpace(h[len(scheme):])
}

package middleware

import (
	"context"
	"net/http"
)

func contextWithCookie(r *http.Request) context.Context {
	return context.WithValue(r.Context(), cookieAuthKey{}, true)
}

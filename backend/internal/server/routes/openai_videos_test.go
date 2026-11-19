package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIVideoLookupRoutesAreScopedToTheirTasks(t *testing.T) {
	const id = "video_proxy_2014b840-5350-4000-8000-100f38f89d5c"
	router := newGatewayRoutesTestRouter(service.PlatformOpenAI)
	for _, path := range []string{"/v1/videos/" + id, "/v1/videos/" + id + "/content", "/videos/" + id + "/content"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		// This router provides a key but no user. Reaching this error proves dispatch.
		require.Contains(t, w.Body.String(), "User context not found", path)
	}

	for _, path := range []string{"/v1/videos/foreign-task/content", "/v1/videos/generations/" + id + "/content"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, w.Code, path)
		require.Contains(t, w.Body.String(), "Videos API is not supported for this platform", path)
	}
}

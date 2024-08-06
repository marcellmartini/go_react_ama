package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/marcellmartini/go_react_ama/internal/store/pgstore"
)

type apiHandler struct {
	querie *pgstore.Queries
	router *chi.Mux
}

func (h apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.router.ServeHTTP(w, r)
}

func NewHandler(q *pgstore.Queries) http.Handler {
	api := apiHandler{
		querie: q,
	}

	router := chi.NewRouter()
	api.router = router

	return api
}

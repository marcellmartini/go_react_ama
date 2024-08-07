package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"

	"github.com/marcellmartini/go_react_ama/internal/store/pgstore"
)

type apiHandler struct {
	querie      *pgstore.Queries
	router      *chi.Mux
	subscribers map[string]map[*websocket.Conn]context.CancelFunc
	mutex       *sync.Mutex
	upgrader    websocket.Upgrader
}

func (h apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.router.ServeHTTP(w, r)
}

func NewHandler(q *pgstore.Queries) http.Handler {
	api := apiHandler{
		querie:      q,
		upgrader:    websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		subscribers: make(map[string]map[*websocket.Conn]context.CancelFunc),
		mutex:       &sync.Mutex{},
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID, middleware.Recoverer, middleware.Logger)

	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PACTH"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	router.Get("/subscribe/{room_id}", api.handleSubscribe)

	router.Route("/api", func(r chi.Router) {
		r.Route("/rooms", func(r chi.Router) {
			r.Post("/", api.handleCreateRoom)
			r.Get("/", api.handleGetRooms)

			r.Route("/{room_id}/messages", func(r chi.Router) {
				r.Post("/", api.handleCreateRoomMessage)
				r.Get("/", api.handleGetRoomMessages)

				r.Route("/{message_id}", func(r chi.Router) {
					r.Get("/", api.handleGetRoomMessage)
					r.Patch("/react", api.handleReactToMessage)
					r.Delete("/react", api.handleRemoveReactFromMessage)
					r.Patch("/answer", api.handleMarkMessageAsAnswerd)
				})
			})
		})
	})

	api.router = router
	return api
}

const (
	MessageKindMessageCreated = "message_created"
)

type MessageMessageCreated struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

type Message struct {
	Kind   string `json:"kind"`
	Value  any    `json:"value"`
	RoomID string `json:"-"`
}

func (h apiHandler) notifyClients(msg Message) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	subscribers, ok := h.subscribers[msg.RoomID]
	if !ok || len(subscribers) == 0 {
		return
	}

	for conn, cancel := range subscribers {
		if err := conn.WriteJSON(msg); err != nil {
			slog.Error("failed to send messege to client", "error", err)
			cancel()
		}
	}
}

func (h apiHandler) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")
	roomID, err := uuid.Parse(rawRoomID)
	if err != nil {
		http.Error(w, "invalid room id", http.StatusBadRequest)
		return
	}

	_, err = h.querie.GetRoom(r.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "room not found", http.StatusBadRequest)
		}

		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	connection, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("failed to upgrade connection", "error", err)
		http.Error(w, "failed to upgrade to ws connection", http.StatusBadRequest)
		return
	}
	defer connection.Close()

	ctx, cancel := context.WithCancel(r.Context())

	h.mutex.Lock()
	if _, ok := h.subscribers[rawRoomID]; !ok {
		h.subscribers[rawRoomID] = make(map[*websocket.Conn]context.CancelFunc)
	}
	slog.Info("new client connected", "room_id", rawRoomID, "client_ip", r.RemoteAddr)
	h.subscribers[rawRoomID][connection] = cancel
	h.mutex.Unlock()

	<-ctx.Done()

	h.mutex.Lock()
	delete(h.subscribers[rawRoomID], connection)
	h.mutex.Unlock()
}

func (h apiHandler) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	type _body struct {
		Theme string `json:"theme"`
	}

	var body _body
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	roomID, err := h.querie.InsertRoom(r.Context(), body.Theme)
	if err != nil {
		slog.Error("failed to insert room", "error", err)
		http.Error(w, "something wet error", http.StatusBadRequest)
		return
	}

	type response struct {
		ID string `json:"id"`
	}

	data, errJson := json.Marshal(response{ID: roomID.String()})
	if errJson != nil {
		slog.Error("fail to marshal json", "error", err)
		http.Error(w, "something wet error", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, errWrite := w.Write(data)
	if errWrite != nil {
		slog.Error("fail to write data", "error", err)
		http.Error(w, "something wet error", http.StatusBadRequest)
		return
	}
}

func (h apiHandler) handleGetRooms(w http.ResponseWriter, r *http.Request) {}

func (h apiHandler) handleCreateRoomMessage(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")
	roomID, err := uuid.Parse(rawRoomID)
	if err != nil {
		http.Error(w, "invalid room id", http.StatusBadRequest)
		return
	}

	_, err = h.querie.GetRoom(r.Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "room not found", http.StatusBadRequest)
		}

		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type _body struct {
		Message string `json:"message"`
	}

	var body _body
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	messageID, err := h.querie.InsertMessage(
		r.Context(),
		pgstore.InsertMessageParams{RoomID: roomID, Message: body.Message},
	)
	if err != nil {
		slog.Error("failed to insert message", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ID string `json:"id"`
	}

	data, errJson := json.Marshal(response{ID: messageID.String()})
	if errJson != nil {
		slog.Error("fail to marshal json", "error", err)
		http.Error(w, "something wet error", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, errWrite := w.Write(data)
	if errWrite != nil {
		slog.Error("fail to write data", "error", err)
		http.Error(w, "something wet error", http.StatusBadRequest)
		return
	}

	go h.notifyClients(Message{
		Kind:   MessageKindMessageCreated,
		RoomID: rawRoomID,
		Value: MessageMessageCreated{
			ID:      messageID.String(),
			Message: body.Message,
		},
	})
}

func (h apiHandler) handleGetRoomMessages(w http.ResponseWriter, r *http.Request) {}

func (h apiHandler) handleGetRoomMessage(w http.ResponseWriter, r *http.Request) {}

func (h apiHandler) handleReactToMessage(w http.ResponseWriter, r *http.Request) {}

func (h apiHandler) handleRemoveReactFromMessage(w http.ResponseWriter, r *http.Request) {}

func (h apiHandler) handleMarkMessageAsAnswerd(w http.ResponseWriter, r *http.Request) {}

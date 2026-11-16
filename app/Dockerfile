# ---------- 1. stage: build ----------
FROM golang:1.27.4-alpine3.24 AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o notes-api .

# ---------- 2. stage: výsledný image ----------
FROM alpine:3.24.3
RUN adduser -D -H -u 10001 appuser
WORKDIR /app
COPY --from=build /app/notes-api .
USER 10001
EXPOSE 8080
CMD ["/app/notes-api"]
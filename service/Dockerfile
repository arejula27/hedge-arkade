FROM golang:1.26-alpine AS build
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o api ./cmd/api

FROM alpine:3.22 AS prod
WORKDIR /app
COPY --from=build /app/api /app/api
CMD ["./api"]

FROM node:22-alpine AS frontend_builder
WORKDIR /frontend

COPY frontend/package*.json ./
RUN npm ci
COPY frontend/. .
RUN npm run build

FROM node:22-alpine AS frontend
RUN npm install -g serve
COPY --from=frontend_builder /frontend/dist /app/dist
EXPOSE 5173
CMD ["serve", "-s", "/app/dist", "-l", "5173"]

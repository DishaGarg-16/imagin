FROM golang:1.22 AS builder

WORKDIR /src
COPY . .
RUN go build -o /app

FROM alpine:3.19

RUN apk add --no-cache ca-certificates
COPY --from=builder /app /app
ENV APP_ENV=production
WORKDIR /
USER nobody
EXPOSE 8080
ENTRYPOINT ["/app"]

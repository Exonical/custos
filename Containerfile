# syntax=docker/dockerfile:1

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/custos ./cmd/custos

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/custos /custos
USER 65532
EXPOSE 8443 9090
ENTRYPOINT ["/custos"]
CMD ["serve"]

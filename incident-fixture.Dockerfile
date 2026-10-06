FROM golang:1.24 AS build
WORKDIR /src
COPY cmd/incident-fixture ./cmd/incident-fixture
RUN CGO_ENABLED=0 GO111MODULE=off go build -o /fixture ./cmd/incident-fixture

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /fixture /fixture
ENTRYPOINT ["/fixture"]

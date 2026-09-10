# syntax=docker/dockerfile:1
FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/alicloud-exporter .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/alicloud-exporter /alicloud-exporter
EXPOSE 9525
USER nonroot:nonroot
ENTRYPOINT ["/alicloud-exporter"]
CMD ["--config=/etc/alicloud-exporter/config.yaml"]

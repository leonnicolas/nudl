FROM golang:1.23-bookworm AS build

RUN apt-get update && apt-get install libusb-1.0-0-dev  -y

WORKDIR /nudl

COPY go.mod go.sum /nudl/
RUN go mod download

COPY main.go /nudl
RUN ls -la
WORKDIR /nudl
RUN go build -o nudl

FROM debian:bookworm-slim
RUN apt-get update && apt-get install libusb-1.0-0-dev  -y
COPY --from=build /nudl/nudl .
ENTRYPOINT ["./nudl"]

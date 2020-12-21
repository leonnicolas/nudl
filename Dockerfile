FROM golang:buster as build
RUN apt-get update && apt-get install libusb-1.0-0-dev  -y
COPY . /nudl
WORKDIR /nudl
RUN go build --mod=vendor -o nudl

FROM debian:10-slim
RUN apt-get update && apt-get install libusb-1.0-0-dev  -y
COPY --from=build /nudl/nudl .
ENTRYPOINT ["./nudl"]

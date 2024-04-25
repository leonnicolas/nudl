FROM golang:1.22-bullseye as build
RUN apt-get update && apt-get install libusb-1.0-0-dev  -y
COPY . /nudl
WORKDIR /nudl
RUN go build --mod=vendor -o nudl

FROM debian:bullseye-slim
RUN apt-get update && apt-get install libusb-1.0-0-dev  -y
COPY --from=build /nudl/nudl .
ENTRYPOINT ["./nudl"]

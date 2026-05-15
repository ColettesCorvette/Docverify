# --- Compilation --- 
FROM --platform=linux/arm64 golang:1.25-trixie AS builder
WORKDIR /usr/local/app
COPY go.mod .
RUN go mod download
COPY . .
RUN go build -o /docverify ./cmd/docverify

# --- Image finale ---
FROM --platform=linux/arm64 debian:trixie-slim
WORKDIR /usr/local/app

RUN apt-get update && apt-get install -y \
    imagemagick \
    poppler-utils \
    tesseract-ocr \
    tesseract-ocr-fra \
    tesseract-ocr-osd \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /docverify .
COPY *.json ./

EXPOSE 8680
ENTRYPOINT ["/usr/local/app/docverify"]
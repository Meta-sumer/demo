# Build context: server-go/
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seed ./cmd/seed \
 && mkdir -p /out/uploads

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /out/seed /app/
# The uploads volume takes this directory's owner when it is first created.
# Without it the volume is root's and the nonroot server cannot save
# deposit and withdrawal proofs.
COPY --from=build --chown=65532:65532 /out/uploads /app/uploads
ENV NODE_ENV=production PORT=4000 UPLOAD_DIR=/app/uploads
EXPOSE 4000
USER nonroot
ENTRYPOINT ["/app/server"]

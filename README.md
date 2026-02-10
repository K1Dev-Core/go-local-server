# Go Local Server

A next-generation local development stack manager for macOS - an alternative to XAMPP or Laragon.

## Features

- **Docker-Only Stack**: Manage services via Docker Compose
  - Apache + PHP
  - MySQL
  - phpMyAdmin
- **Project Management**: Create, edit, delete, and import projects
- **Project Templates**: Generate Simple PHP or PHP MVC template projects
- **Auto Apache VHost Generation**: Automatically create Apache virtual hosts for projects
- **Clean UI**: macOS desktop application using Fyne

## Project Structure

```
go-local-server/
├── cmd/golocal/                # Main application entry point
│   └── main.go
├── internal/
│   ├── config/             # Configuration management
│   ├── services/           # Docker service controllers
│   ├── projects/           # Project management
│   ├── dns/                # DNS server for wildcard domains
├── pkg/
│   ├── apache/             # Apache vhost generator
│   └── php-mvc-main/        # MVC template files
├── apache/                  # Apache Dockerfile and vhost templates
├── php/                     # PHP config (php.ini)
├── docker-compose.yml        # Docker Compose stack
├── scripts/
│   ├── build-app.sh         # Build .app bundle + zip
│   └── build-dmg.sh         # Build DMG installer
└── go.mod                  # Go module definition
```

## Requirements

- macOS
- Docker Desktop
- Go 1.21+ (for building from source)

## Building

```bash
go mod download

go build -o bin/GoLocalServer ./cmd/golocal

# Build a macOS .app bundle
./scripts/build-app.sh

# Build a macOS DMG installer
./scripts/build-dmg.sh
```

## Running

- Install from the DMG (`dist/GoLocalServer-vX.Y.Z-macOS.dmg`)
- Open **GoLocalServer.app** from Applications

## Configuration

Configuration is stored in `~/Library/Application Support/GoLocalServer/`:
- `config.json` - Application settings
- `projects/` - Project definitions
- `logs/` - Service logs
- `docker/` - Docker resources copied by the app (used to avoid macOS Docker file-sharing issues)

## Usage

1. Launch the application
2. Ensure Docker Desktop is running
3. Go to **Services** and start the stack (or use Docker Up)
4. Go to **Projects**
5. Add / Import projects

## Default Ports

- HTTP: 80
- MySQL: 3306
- phpMyAdmin: 8081

## License

MIT License

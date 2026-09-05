# Grok2API

[简体中文](README.md) | English

A Go and React API gateway that manages Grok Build, Grok Web, and Grok Console accounts through OpenAI- and Anthropic-compatible interfaces.

## Features

- Account imports, quota synchronization, credential renewal, and model routing.
- Chat, image, video, and audio APIs, depending on account and model capabilities.
- Client keys, request audits, proxy configuration, and a web admin console.
- GitHub update checks and installation from the console, with progress and startup rollback.

## Quick Start

Requires Docker and Docker Compose. Supported platforms: `linux/amd64` and `linux/arm64`. After downloading or cloning this project, run from its root directory:

```bash
cp config.example.yaml config.yaml
openssl rand -hex 32
openssl rand -base64 32
```

Put the generated Hex and Base64 secrets into `config.yaml` respectively, and set an administrator password:

```yaml
secrets:
  jwtSecret: "replace-with-the-generated-hex-secret"
  credentialEncryptionKey: "replace-with-the-generated-base64-secret"

bootstrapAdmin:
  username: "admin"
  password: "replace-with-a-strong-password"
```

Build and start the service:

```bash
docker compose up -d --build grok2api
docker compose logs -f grok2api
```

Open [http://127.0.0.1:8000](http://127.0.0.1:8000) and sign in with your configured administrator credentials. The default single-instance deployment uses SQLite and stores databases, media, and updates in the `grok2api-data` volume.

## Usage

1. Sign in and import or authorize Build, Web, or Console accounts.
2. Wait for synchronization and review available models in the console.
3. Create a client key and set your client's API base URL to `http://YOUR_SERVER:8000/v1`.

List available models:

```bash
curl http://127.0.0.1:8000/v1/models \
  -H "Authorization: Bearer YOUR_CLIENT_KEY"
```

Use a returned model name to send a request:

```bash
curl http://127.0.0.1:8000/v1/responses \
  -H "Authorization: Bearer YOUR_CLIENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"YOUR_MODEL","input":"Hello","stream":true}'
```

Additional endpoints and examples are available under Documentation in the console.

## Web Updates

Open **Settings -> About & updates**, save your public GitHub repository, check for updates, and select **Install update**.

Existing servers need an initial deployment containing this feature. Installation supports managed, single-instance Linux deployments; Docker enables this by default. Updating briefly restarts the service. Startup failure restores application files but does not roll back the database.

The repository must publish a stable Release containing full installation bundles and checksums. Source commits alone are not installable updates. A GitHub Actions release workflow is included; see the [web update and publishing guide](backend/docs/self-update.md).

## Configuration

- See [config.example.yaml](config.example.yaml) for startup configuration. Runtime settings are available in the console.
- Change the administrator password after first sign-in. Preserve `credentialEncryptionKey` after storing accounts so existing credentials remain readable.
- Back up configuration, databases, and media. Do not commit credentials or account data, or delete persistent volumes.
- Use HTTPS for public deployments and set `auth.secureCookies: true`.
- Multiple replicas require PostgreSQL, Redis, and shared media storage; see the [backend guide](backend/README.md).

## Development

- [Backend setup and development](backend/README.md)
- [Frontend setup and builds](frontend/README.md)
- [Optional egress quality guard](tools/egress-quality-guard/README.md)

## License

This project uses the [MIT License](LICENSE) and retains the original copyright and license notice.

## Source and Acknowledgments

This project is based on the open-source **Grok2API** project.

Original GitHub repository: [https://github.com/chenyme/grok2api](https://github.com/chenyme/grok2api)

Thanks to **Chenyme** and all contributors for the open-source code, documentation, and maintenance that made this work possible.

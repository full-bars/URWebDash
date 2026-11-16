## Quick install

Linux, macOS and WSL2:

```bash
curl -fsSL https://raw.githubusercontent.com/full-bars/URWebDash/master/install.sh | bash
```

Docker:

```bash
docker run -d --name urwebdash \
  -p 127.0.0.1:3001:3001 \
  -v urwebdash-data:/data \
  -e URNETWORK_AUTH_CODE='AUTH_CODE_HERE' \
  ghcr.io/full-bars/urwebdash
```

Replace `AUTH_CODE_HERE` with a code from https://ur.io and keep the single quotes. To update, run the install command again or pull the new image.

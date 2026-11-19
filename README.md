# mirakc-ui

A Web UI for [mirakc](https://github.com/mirakc/mirakc).

Browse the program guide, search programs, schedule recordings, and watch live —
all from your browser.

## Screenshots

> The screenshots use fictional channels and programs; no real broadcast data is
> shown.

<table>
  <tr>
    <td align="center" width="50%">
      <img src="docs/screenshots/program.png" alt="Program guide" width="100%"><br>
      <sub>Program guide</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/screenshots/search.png" alt="Search" width="100%"><br>
      <sub>Search</sub>
    </td>
  </tr>
  <tr>
    <td align="center" width="50%">
      <img src="docs/screenshots/detail.png" alt="Program details and recording reservation" width="100%"><br>
      <sub>Program details &amp; recording reservation</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/screenshots/watch.png" alt="Live viewing" width="100%"><br>
      <sub>Live viewing</sub>
    </td>
  </tr>
</table>

## Features

- Browse the TV program guide per channel type (GR / BS / CS / SKY), with a
  current-time line and genre-coloured cells.
- Search programs by keyword, and review your recording reservations in one
  place.
- Schedule and cancel recording reservations from the program details.
- Watch live in the browser: server-side transcoding to H.264 / AAC, audio
  track and quality switching, ARIB caption overlay, and a live-comment panel.

## Install

First, enable
[mirakc's recording reservation feature](https://mirakc.github.io/dekiru-mirakc/latest/config/recording.html).

Add the following to `docker-compose.yml`.

```yml:docker-compose.yml
services:
  mirakc:
    image: mirakc/mirakc:alpine
    init: true
    restart: unless-stopped
    ports:
      - 40772:40772
    volumes:
      - ./config.yml:/etc/mirakc/config.yml:ro
    environment:
      TZ: Asia/Tokyo
      RUST_LOG: info
## from:
  ui:
    image: ghcr.io/mxalbert1996/mirakc-ui:latest
    ports:
      - 8888:8000
    volumes:
      - ./mirakc-ui-data:/app/data
    environment:
      MIRAKC_URL: http://mirakc:40772
## to:
```

mirakc-ui stores its settings (e.g. keyword recording rules) in a SQLite
database under `/app/data`. Mount this directory as shown above so the settings
survive container recreation.

On Apple AVE hardware, add `V4L2_CQ: 1` to the `environment:` block to encode
with constant QP instead of a target bitrate (see
[docs/transcode-quality.md](docs/transcode-quality.md)). With this setting,
hardware encoders that lack constant QP fail their probe and the stream falls
back to libx264.

### Authentication (optional)

By default mirakc-ui serves everything without authentication, for deployments
behind a reverse proxy or on a trusted LAN. To have mirakc-ui itself require a
login, set `AUTH_TOKEN` to a long random ASCII string in the `environment:`
block:

```yml
MIRAKC_URL: http://mirakc:40772
AUTH_TOKEN: paste-a-random-token
```

Generate one with e.g. `openssl rand -base64 24`. With it set:

- The browser shows a native login prompt when you first open the UI (any
  user name works; the token is the password). API clients can use
  `Authorization: Bearer <token>` or `X-Auth-Token: <token>` instead.
- Live viewing and the live-comment panel fetch short-lived one-time tickets
  automatically — EventSource and mpegts.js cannot send headers, so nothing
  needs configuring client-side.
- Use an ASCII token: non-ASCII characters work with `Bearer`/`X-Auth-Token`
  but cannot be entered in the Basic-auth password field.

A blank or whitespace-only `AUTH_TOKEN` (e.g. an empty compose interpolation)
leaves authentication **disabled**; the server logs a warning at startup when
it sees one. If mirakc-ui is reachable from an untrusted network, set
`AUTH_TOKEN` or front it with an authenticating reverse proxy.

After launching, open <http://localhost:8888/>. It opens the program guide; from
there you can search programs, schedule recordings, and start live viewing.

## Third-Party Notices

mirakc-ui itself is MIT licensed (see [LICENSE](./LICENSE)). The live-comment
feature references the following third-party works, each used as a reference
implementation or as derived data rather than bundled verbatim. All are MIT
licensed.

- **[NDGRClient](https://github.com/tsukumijima/NDGRClient)** — The NDGR
  (Niconico's post-2024/08 message server) receive flow in
  `server/lib/comments/sources/nicolive.ts` is implemented with reference to it.
  The nicolive channel IDs in `client/assets/datas/live-comment-defaults.ts`
  also originate from its channel-ID map.
- **[nicolive-comment-protobuf](https://github.com/n-air-app/nicolive-comment-protobuf)**
  — The Protobuf field numbers decoded in `server/lib/comments/ndgr.ts` come
  from its schema definitions (`proto/dwango/nicolive/chat/`).
- **[NX-Jikkyo](https://github.com/tsukumijima/NX-Jikkyo)** and
  **[KonomiTV](https://github.com/tsukumijima/KonomiTV)** — The legacy Niconico
  comment-server protocol in `server/lib/comments/sources/nx-jikkyo.ts` is
  implemented with reference to KonomiTV's `LiveCommentManager.ts` and
  NX-Jikkyo.

<details>
<summary>MIT License (NDGRClient / NX-Jikkyo / KonomiTV: Copyright (c) tsukumi; nicolive-comment-protobuf: Copyright (c) n-air-app)</summary>

```
MIT License

Copyright (c) 2021-2026 tsukumi
Copyright (c) 2024 n-air-app

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

</details>

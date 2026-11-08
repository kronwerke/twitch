```
 _  __                                 _
| |/ /_ __ ___  _ ____      _____ _ __| | _____
| ' /| '__/ _ \| '_ \ \ /\ / / _ \ '__| |/ / _ \
| . \| | | (_) | | | \ V  V /  __/ |  |   <  __/
|_|\_\_|  \___/|_| |_|\_/\_/ \___|_|  |_|\_\___|

                        t w i t c h
```

**The Twitch bot of the Kronwerke server: it sits in every streamer's chat, answers commands, posts timers, and shows every chat in one dashboard.**

[![ci](https://github.com/kronwerke/twitch/actions/workflows/ci.yml/badge.svg)](https://github.com/kronwerke/twitch/actions/workflows/ci.yml)
![status](https://img.shields.io/badge/status-early-orange)
![go](https://img.shields.io/badge/go-1.24-blue)
![licence](https://img.shields.io/badge/licence-MIT-green)
[![made by](https://img.shields.io/badge/made%20by-Elchi-black)](https://github.com/Elchi-dev)

## Overview

Kronwerke is played by a group of streamers at once. Their viewers ask the same things in every chat: when does it start, how do I get in, who else is live. The bot answers that in all of them, with one set of commands that the team changes in one place.

- **Commands** like `!start`, `!discord` or `!multi`, with placeholders (`{countdown}`, `{live}`, `{discord}` ...), aliases, cooldowns, mod only and per channel. A new command works the moment it is saved.
- **Timers** that post every N minutes, only when the channel is live and enough was written since the last one.
- **Live chat** of every channel in one view, filterable, with writing as the bot.
- **Who is live**, for the dashboard and for the website (`GET /api/live`, CORS open).
- **Access** with Twitch: only accounts on the list, bound to their Twitch id the first time they sign in.

Go, standard library only. One binary, one JSON state file. It updates itself from the releases of this repository. The Discord half of Kronwerke is [kronwerke/bot](https://github.com/kronwerke/bot).

## API

Everything the dashboard does is an API call. Without a browser, send `Authorization: Bearer <TTV_ADMIN_TOKEN>`.

| Call | What |
| --- | --- |
| `GET /api/overview` | Bot account, chat state, channels with live state, commands, timers, settings, accounts |
| `GET /api/chat?n=200&channel=` | The last chat lines |
| `GET /api/events` | Server sent events: every chat line and every change |
| `POST /api/chat` `{"channel","text"}` | Write as the bot (placeholders work) |
| `POST /api/channels` `{"login","note"}` | Add a channel, the bot joins at once |
| `PATCH /api/channels/{login}` `{"enabled","website","note"}` | Change it |
| `DELETE /api/channels/{login}` | Remove it |
| `PUT /api/commands/{name}` | Create or replace a command (`name` in the body renames) |
| `DELETE /api/commands/{name}` | Remove it |
| `PUT /api/timers/{id}` | Create (`new`) or replace a timer |
| `DELETE /api/timers/{id}` | Remove it |
| `PATCH /api/settings` `{"start","discord","website","server"}` | What the placeholders put in |
| `POST /api/users` `{"login"}`, `DELETE /api/users/{login}` | Dashboard access |
| `DELETE /api/bot` | Disconnect the bot account |
| `POST /api/preview` `{"text","channel"}` | A text with its placeholders filled |
| `GET /api/log` | What happened, newest last |
| `GET /api/live` | Public: who is live, for the website |

## Running it

[docs/DEPLOY.md](docs/DEPLOY.md). Configuration in [deploy/config.example.env](deploy/config.example.env).

## Licence

MIT

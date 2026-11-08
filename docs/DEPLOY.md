# Deploy

The service runs as a systemd unit on a Linux server (amd64) behind Caddy. It needs outbound HTTPS to `id.twitch.tv`, `api.twitch.tv`, `api.github.com` and `github.com`, and TLS to `irc.chat.twitch.tv:6697`. It listens only on loopback, port 13040.

The install is done once. After that, new versions arrive through releases by themselves.

## Before

- A Twitch account for the bot (its name is what chat shows).
- An application on [dev.twitch.tv/console](https://dev.twitch.tv/console): category **Chat Bot**, OAuth redirect URL exactly `https://ttv.kronwerke.com/auth/callback`, client type **Confidential**. Keep the Client ID and make a client secret.
- A DNS record `ttv.kronwerke.com` to the server.

## 1. Download a release

```bash
v=v0.1.0
rm -rf ~/kronwerke-twitch && mkdir ~/kronwerke-twitch && cd ~/kronwerke-twitch \
  && curl -fsSL --remote-name-all https://github.com/kronwerke/twitch/releases/download/$v/{kronwerke-twitch-linux-amd64,install.sh,rollback-guard.sh,kronwerke-twitch.service,config.example.env,SHA256SUMS} \
  && sha256sum -c SHA256SUMS
```

Pass: every file `OK`.

## 2. Install

```bash
cd ~/kronwerke-twitch && sudo sh install.sh
```

Pass: `installed v0.1.0`. This creates the user `kronwerke-twitch`, `/srv/projects/kronwerke-twitch/{bin,state}`, the rollback guard, the unit and `/etc/kronwerke-twitch/twitch.env` from the example (an existing one is kept).

## 3. Application and API token

```bash
printf 'Client ID: ' && read -r id && printf 'Client secret: ' && read -rs sec && echo \
  && tok=$(sudo sed -n 's/^KW_ADMIN_TOKEN=//p' /etc/kronwerke-bot/bot.env) \
  && printf '%s\n%s\n%s\n' "$id" "$sec" "$tok" | sudo sh -c 'IFS= read -r id; IFS= read -r sec; IFS= read -r tok; f=/etc/kronwerke-twitch/twitch.env; sed -i -e "s|^TTV_CLIENT_ID=.*|TTV_CLIENT_ID=$id|" -e "s|^TTV_CLIENT_SECRET=.*|TTV_CLIENT_SECRET=$sec|" -e "s|^TTV_ADMIN_TOKEN=.*|TTV_ADMIN_TOKEN=$tok|" $f' \
  && unset id sec tok && sudo grep -c '^TTV_CLIENT_ID=.\{10,\}\|^TTV_CLIENT_SECRET=.\{10,\}\|^TTV_ADMIN_TOKEN=.\{32,\}' /etc/kronwerke-twitch/twitch.env
```

Pass: `3`. The API token is the same as the Discord bot's admin token, so the team's tools reach both with one secret.

## 4. Caddy

Add to the Caddyfile:

```
ttv.kronwerke.com {
	encode zstd gzip
	reverse_proxy 127.0.0.1:13040 {
		flush_interval -1
	}
}
```

`flush_interval -1` keeps the live chat (server sent events) flowing. Then `sudo systemctl reload caddy`.

## 5. Start

```bash
sudo systemctl enable --now kronwerke-twitch && sleep 3 \
  && systemctl is-active kronwerke-twitch && curl -s 127.0.0.1:13040/healthz
```

Pass: `active`, then `ok`. Open https://ttv.kronwerke.com, sign in with Twitch as the owner (`TTV_OWNER`), then **Bot mit Twitch verbinden** with the bot's account.

## Updates

Every release (a tag `v*`, or the release workflow with a version) is installed by the running service within ten minutes: download, SHA-256 check, one test run, swap, restart. If the new binary fails three starts in a row before it reaches Twitch chat, the guard puts the previous one back and that tag is never installed again.

## Where things are

- `/etc/kronwerke-twitch/twitch.env`: the application, the owner, the API token (`sudo systemctl restart kronwerke-twitch` after a change)
- `/srv/projects/kronwerke-twitch/state/state.json`: accounts, the bot's tokens, channels, commands, timers, sessions (mode 600)
- `journalctl -u kronwerke-twitch`: the log

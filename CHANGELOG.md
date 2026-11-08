# Changelog

## 0.1.0

First version.

- Sits in the chats of the channels the team picks, answers commands and posts timers, as its own Twitch account.
- Dashboard at ttv.kronwerke.com: sign in with Twitch (only accounts on the list, bound to their Twitch id at the first sign-in), connect the bot account with Twitch as well, add and switch channels, every chat live in one view with writing as the bot, commands and timers with placeholders and a preview, settings, access for more accounts, a log. Every change takes effect at once.
- `GET /api/live` for the website: who of the channels marked for it is live, with title, viewers and thumbnail.
- The whole dashboard is an API too, with a bearer token (`TTV_ADMIN_TOKEN`).
- Updates itself from the releases of this repository, with a rollback after three failed starts.

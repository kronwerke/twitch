# Changelog

## 0.4.1

- Long messages are cut at a character boundary, not in the middle of an umlaut, and leave room for the invisible marker. (R3-14)

## 0.4.0

The bot works without moderator rights:
- It reads each channel's chat settings and whether it is moderator or VIP there.
- Slow mode: a message waits until the bot may write again (the chat shows "wartet 30 s").
- The same text twice within 30 seconds, or in a unique chat, gets an invisible marker, so Twitch does not drop it as a duplicate.
- In a subs only or emote only chat, and in a followers only chat that refused it, the bot does not write into the void; the channel says why it is silent.
- A message Twitch did not post shows red and struck through, with the reason in German.
- A channel that refuses a link of the bot gets the message once more with the link written as `discord(.)gg/...`, and every later link there too.
- The channels view shows slow mode, followers only, unique chat, blocked links and "Bot ist Mod".

## 0.3.0

- Roles. The owner keeps everything; editors change commands, timers and channels and write in chat; viewers only look. Settings, access and the bot account are the owner's. The owner switches a user between editor and viewer on the access page; every change is in the log.
- `{args}` is cleaned before the bot repeats it: no links, no leading `/`, `.` or `!`, at most 60 characters. A command whose text starts using `{args}` is set to moderators only (the owner can open it again).
- The season start follows the server: without a start set here, the bot reads it from kronwerke.com's status (Kronwerke Core's `seasonStart`), every ten minutes. The old fixed seed is cleared once. The setting `lang` = `en` writes `{start}` and `{countdown}` in English.
- Updates install only releases whose SHA256SUMS carries the release key's signature (`SHA256SUMS.sig`, Ed25519, pinned in the binary).

## 0.2.0

A new dashboard. Same features, built again so it loads every time and feels calm to use.

- It always shows something: grey placeholders while it loads, a clear message with a retry (and an automatic one) when the server cannot be reached, and the sign in page when the session has ended, with a note why.
- It recovers by itself. When the live connection drops, for example while the service restarts for an update, a small notice says so and counts down to the next try; once the server is back it fetches everything it missed and says so. When a new version is running, it offers to reload.
- Live updates no longer rebuild the page. Lists change in place, so a form you are typing in, a selection or the scroll position stay where they are.
- Changes show at once: switches flip immediately and flip back with a message if the server says no, saved and removed items get a short confirmation, deleting asks in a proper dialog.
- Overview: a live countdown to the season start, the bot account, four numbers at a glance, who is live with preview pictures, and the latest chat lines.
- Live chat fills the screen and stays at the bottom unless you scroll up; then a button shows how many new lines came in. Filter by channel with one click, pause and resume, write as the bot with Enter.
- Commands and timers: list and editor side by side, aliases as tags, placeholders to click into the text, a live preview, channels to pick, unsaved changes are marked and kept from being lost.
- Settings with a date picker for the start and a save bar that appears when something changed, plus a system card with version, uptime and the redirect URL to copy.
- Sidebar on wide screens, a tab bar on phones, editors as full screen sheets on phones. Keyboard friendly (Enter, Escape, Ctrl+Enter to save) with visible focus. Animations stay subtle and switch off when the system asks for reduced motion.
- Schibsted Grotesk as the typeface, served from the dashboard itself.
- The live stream (`GET /api/events`) now sends a named `ping` event every 25 seconds, so the dashboard notices a dead connection. Plain message listeners do not see it.

## 0.1.1

- The dashboard stayed empty, without the button to connect the bot, as long as no channel was added: the overview sent an empty list as null.

## 0.1.0

First version.

- Sits in the chats of the channels the team picks, answers commands and posts timers, as its own Twitch account.
- Dashboard at ttv.kronwerke.com: sign in with Twitch (only accounts on the list, bound to their Twitch id at the first sign-in), connect the bot account with Twitch as well, add and switch channels, every chat live in one view with writing as the bot, commands and timers with placeholders and a preview, settings, access for more accounts, a log. Every change takes effect at once.
- `GET /api/live` for the website: who of the channels marked for it is live, with title, viewers and thumbnail.
- The whole dashboard is an API too, with a bearer token (`TTV_ADMIN_TOKEN`).
- Updates itself from the releases of this repository, with a rollback after three failed starts.

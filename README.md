lierc
=====

The IRC side of [relaychat](https://relaychat.party/), a web IRC client that
stays connected when you close the tab. Try it at <https://relaychat.party/>.

Questions or ideas? Come talk in [#relaychat](https://relaychat.party/join/?network=relaychat&channel=relaychat).

- `cmd/liercd`: keeps each user's IRC connections open, whether or not
  they're in a browser.
- `cmd/lierc-identd`: answers ident lookups for liercd's connections.
- `pkg/ircwire`: the message format liercd publishes, and the addresses
  it won't dial; lierc-api's logger imports it.

The HTTP API, event stream and logger (which stores IRC traffic in
Postgres and sends highlight notifications) are in
[lierc-api](https://github.com/lierc/lierc-api), the web UI in
[lierc-web](https://github.com/lierc/lierc-web), and a local
development stack in
[lierc-docker-compose](https://github.com/lierc/lierc-docker-compose).

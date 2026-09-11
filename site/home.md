---
title: "Documentation"
---

Skrog runs the **upstream open source Docker Engine** on Windows, inside WSL2.
No licence fees, no Electron, no Kubernetes: install once and `docker ps` works
forever, on laptops and CI runners alike.

```
skrog install                                  # engine, context, autostart
docker --context skrog run --rm hello-world
```

New here? The [README](https://github.com/wslkit/skrog#install) has the
install steps and the current status. These pages are the detail behind them —
each one is a markdown file in
[`docs/`](https://github.com/wslkit/skrog/tree/main/docs), so anything wrong
on this site is a pull request away from being right.

# Log: what was done, why, and what it measured

First built on 2026-11-13 from `git log origin/main` (327 commits, to `3e8075f`), the 150 pull requests of that day (merged, closed and open), the 44 releases' notes, the tree at `origin/main` (comments, `docs/`, `boot/*_test.go` row comments), and spin-stack/spin#1206 and spin-stack/open-remote-ssh#8. Hashes are short commit ids on `origin/main`; `#N` is a pull request here; "lab N" is a `lab.yml` run id as the tree or a PR quotes it. Dates are UTC, as the bench fleet and spin keep them. A commit made in UTC−3 can carry the day before. Nothing here was re-measured. Every change that decides something since adds its entry.

Where boot time stands, what each phase costs, how a number is measured, and the tables of what was kept, measured and rejected, and is next are `docs/boot-time.md`. This log is the chronology.

---

## 1. Chronological log

Format: **date · area · refs**, then what was done, why, the result and its status.

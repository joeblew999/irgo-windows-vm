---
title: Writing docs
nav_order: 90
---

# Writing docs

Written by `charter docs` (the same page in every repo that uses it): don't edit it here, change `cmd/charter/docs/writing.md` in [charter](https://github.com/joeblew999/charter). These are the rules a page in `docs/` is held to. `mise run docs:lint` checks what a program can check, and `mise run docs:review` has Claude do the rest.

## Who reads a page

A developer or an agent who is new to this repo and has a job to do. They know the tools in general. They don't know this repo's names, layout or history, and they will act on what the page says.

## What every page must be

1. **One job.** The first paragraph says what the page covers and when to read it. If a page has two jobs, it is two pages.
2. **True at this commit.** It describes the repo as it is. No history ("previously", "we changed", "now"): that is what git and the findings page are for. Nothing planned, except under `plans/`.
3. **Checkable.** Every command is one the reader can paste: a real task from `mise tasks`, shown as typed. Every path exists and is written from the repo's root (`api/contract.go`, not `contract.go`). Every number says where it was measured and when.
4. **One place per fact.** A fact lives on the page that owns it; other pages link to it. A repeated fact goes stale in one of its copies.
5. **In the order the reader needs it.** How to run or use the thing first, then how it works, then limits and reference.
6. **Honest about limits.** What is not done, not tested, or only tested locally is said plainly, next to the claim it limits.

## How to write it

- **Plain words, short sentences.** One idea each. No sales language, no filler.
- **One name per thing.** Use the names from the "What is what" table on the start page, and the same name every time. Define any other term where it first appears.
- **Say what something is before what it is called.** "The tool the tasks run (`charter`)", not "`charter`, the tool...".
- **Tables for mappings and comparisons, bullets for lists, code blocks for commands.** A command gets a comment saying what it does. Bullets start with their subject in bold. No bullet nested more than two deep.
- **Headings a reader would search for:** what the section answers, not a label.
- **Examples are real.** Output shown is output that was produced; names in examples exist.

## How the docs are laid out

A reader comes to use the thing, not to study the repo. So the docs are ordered by what a reader is doing, not by the repo's folders:

| Section | What goes there | It answers |
|---|---|---|
| Home (`README.md`) | What this is, who it is for, the shortest way to something running, where to go next | "Is this for me, and where do I start?" |
| Getting started | One tutorial: from nothing to a working result, every step run as written | "Show me it working" |
| Guides | One task per page, start to finish, for someone who has done the tutorial | "How do I do X?" |
| Concepts | Why it works the way it does, and what follows from that | "Why?" |
| Reference | Every command, task, package, setting: complete, uniform, no narrative | "What exactly is Y?" |
| This repository | How the repo itself is built and kept: rules, internals, findings, plans | "How do I change this project?" |

A page belongs to exactly one section. A guide does not explain why (it links to a concept), a concept does not list flags (it links to reference), and nothing a user needs is only under "This repository".

## What each kind of page is for

| Page | Its job | It must not |
|---|---|---|
| `README.md` (start page) | The index of every page, and what is what: each part, its name, where it lives | Explain how any part works |
| `rules.md` | The rules for changing the repo. Binding, short, each with its reason | Describe the system |
| A page per part | What the part is, how to run and change it, how it works, its limits | Repeat another part's page |
| `findings.md` | Results that were verified: what ran, where, when, and what came out. Newest last | Hold anything not run, or be rewritten after the fact |
| `plans/` | What is not built yet, and why | Describe what exists. When a plan is built, its content moves to the part's page and to findings, and leaves the plan |
| This page | The rules for the pages | |

## The mechanics

- **Front matter first:** `title` (short, for the sidebar), `nav_order`, and `parent` if the page sits under another. The start page (`README.md`) also has `permalink: /`: without it the site has no home page.
- **Links are relative** (`[other-page.md](other-page.md)`), and an anchor must match a heading.
- **A new page gets a row in the start page's table.**
- **No two opening curly braces together, and no curly brace followed by a percent sign:** the site's renderer reads those as template code.
- **No release version in a page:** link `releases/latest`, write `@latest`, or use the placeholder `vX.Y.Z`. A version written into a page is wrong after the next release. (Findings and plans record what was, and may name one.)
- **Don't edit what is generated:** `_config.yml`, `_sass/`, this page.

## When the code changes

The page changes in the same commit. A new task, flag, file or behaviour goes on the page of the part it belongs to; a result that was verified goes in findings; a removed thing is removed from every page (`mise run docs:lint` finds the mentions).

# Git commands for the whole repository. Included by client/Makefile and
# server/Makefile, so they run from either folder but act on the project root.
#   make update            - pull the latest changes from GitHub
#   make push              - commit all changes and push ("update v<version>")
#   make push m="message"  - same, with your own commit message
# Recipes use only `git`, so they work in cmd.exe and sh.

ROOT := ..
MSG  := $(if $(m),$(m),update v$(VERSION))

.PHONY: update push

update:
	git -C $(ROOT) pull --ff-only

push:
	git -C $(ROOT) add -A
	-git -C $(ROOT) commit -m "$(MSG)"
	git -C $(ROOT) pull --rebase
	git -C $(ROOT) push

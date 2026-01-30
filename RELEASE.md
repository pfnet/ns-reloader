# Release workflow

To use GitHub's [immutable
releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases)
the following release workflow is used.

## Update `CHANGELOG.md`

Before the release, ensure that `CHANGELOG.md` is updated with and relevant
changes for the new version.

## Create and push a new tag

```shell
git tag vX.Y.Z
git push origin vX.Y.Z
```

This will trigger a release workflow that builds the binaries and Docker images,
and creates a new draft GitHub Release with the built artifacts attached.

## Edit the draft release

Update the description of the created release. Reference the `CHANGELOG.md` for
user facing changes.

## Publish the release

**Warning:** the release is immutable and cannot be changed after publishing!!

Once the draft release is ready, publish it to make it publicly available.

# OTC Merge [![go.dev reference](https://img.shields.io/badge/go.dev-reference-007d9c?logo=go&logoColor=white&style=for-the-badge)](https://pkg.go.dev/github.com/Nik-U/otcmerge)

Package otcmerge combines TrueType / OpenType font files (.ttf / .otf) into
TrueType / OpenType Collection files (.ttc / .otc).

Individual TTF/OTF files store data in the SFNT format. For historical
reasons, this means that each font file is limited to a maximum of
2<sup>16</sup> glyphs. This is not enough to cover all of unicode, and so a
single font file cannot provide full unicode coverage.

TTC/OTC files are essentially the OpenType version of file archives. Each
collection can contain multiple individual SFNT files within it. Since the
SFNT format is organized as a series of data tables specified as offsets from
the beginning of the file, a merged collection can also de-duplicate data by
pointing to the same table from multiple SFNT "files" contained within the
collection. Aside from this, the font files contained in a collection do not
directly interact. This means that you cannot simply replace a TTF/OTF with a
TTC/OTC in an arbitrary application and expect it to work: the application
must specifically have support for reading collections.

This package exports a single function: `Merge`. This function takes multiple
font files as input and produces an OpenType collection file as output. The
output is given as an `io.WriteSeeker` because the function keeps minimal
internal buffers. If you need the resulting file in memory instead of on disk,
you can use a package like
[github.com/orcaman/writerseeker](https://github.com/orcaman/writerseeker/).

This package is meant to be used as a Go library. If you need a command-line
tool to do this, you can use the `otf2otc` command from the
[Adobe Font Development Kit for OpenType](https://pypi.org/project/afdko/),
which is a far more mature project.

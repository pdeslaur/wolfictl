package advisory

import (
	"context"
	"fmt"
	"sort"

	"github.com/samber/lo"
	"github.com/wolfi-dev/wolfictl/pkg/configs"
	v2 "github.com/wolfi-dev/wolfictl/pkg/configs/advisory/v2"
	"github.com/wolfi-dev/wolfictl/pkg/vuln"
)

// DiscoverAliasesOptions is the set of options for the DiscoverAliases
// function.
type DiscoverAliasesOptions struct {
	// AdvisoryDocs is the Index of advisory documents on which to operate.
	AdvisoryDocs *configs.Index[v2.Document]

	// AliasFinder is the alias finder to use for discovering aliases for the given
	// vulnerabilities.
	AliasFinder AliasFinder

	// SelectedPackages is the set of packages to operate on. If empty, all packages
	// will be operated on.
	SelectedPackages map[string]struct{}
}

// DiscoverAliases queries external data sources for aliases for the
// vulnerabilities described in the selected advisories and updates the advisory
// documents with the discovered aliases.
func DiscoverAliases(ctx context.Context, opts DiscoverAliasesOptions) error {
	documents := opts.AdvisoryDocs.Select().Configurations()

	// If the caller selected specific packages (by name), filter the set of
	// advisory documents to just those packages.
	if len(opts.SelectedPackages) > 0 {
		documents = lo.Filter(documents, func(doc v2.Document, _ int) bool {
			_, ok := opts.SelectedPackages[doc.Package.Name]
			return ok
		})
	}

	for _, doc := range documents {
		for _, adv := range doc.Advisories {
			// If the advisory ID is already a CVE ID, do a lookup and ensure the discovered
			// aliases are present in the advisory's list of aliases.
			if vuln.RegexCVE.MatchString(adv.ID) {
				ghsas, err := opts.AliasFinder.GHSAsForCVE(ctx, adv.ID)
				if err != nil {
					return err
				}
				updatedAliases := lo.Uniq(append(adv.Aliases, ghsas...))
				adv.Aliases = updatedAliases

				u := v2.NewAdvisoriesSectionUpdater(func(doc v2.Document) (v2.Advisories, error) {
					advisories := doc.Advisories.Update(adv.ID, adv)

					// Ensure the package's advisory list is sorted before returning it.
					sort.Sort(advisories)

					return advisories, nil
				})
				err = opts.AdvisoryDocs.Select().WhereName(doc.Name()).Update(u)
				if err != nil {
					return err
				}

				continue
			}

			// If the advisory ID isn't a CVE ID, lookup aliases for this ID, and see if
			// there exists a CVE ID among those aliases. If so, adjust the advisory so that
			// the advisory ID is this CVE ID, and ensure all other IDs are present in the
			// advisory's list of aliases (including the advisory's original ID).
			if vuln.RegexGHSA.MatchString(adv.ID) {
				cve, err := opts.AliasFinder.CVEForGHSA(ctx, adv.ID)
				if err != nil {
					return err
				}
				if cve == "" {
					// No CVE ID was found for this GHSA ID, so there's nothing else we can do here.
					continue
				}

				// Rearrange the data so that the advisory ID is set to the CVE ID, and the GHSA
				// ID, which was originally the value of the advisory ID, is now an alias.
				ghsaFromAdvisoryID := adv.ID
				adv.ID = cve
				adv.Aliases = append(adv.Aliases, ghsaFromAdvisoryID)

				if _, ok := doc.Advisories.Get(cve); ok {
					// This CVE ID is already present in the document as the ID of another advisory.
					// This means we'd end up with two advisories with the same ID, which is not
					// allowed. Rather than try any kind of merging operation, this should be
					// resolved manually, so we'll error out here.
					return DuplicateAdvisoryIDError{
						Package:    doc.Package.Name,
						AdvisoryID: cve,
					}
				}

				ghsas, err := opts.AliasFinder.GHSAsForCVE(ctx, cve)
				if err != nil {
					return err
				}

				updatedAliases := lo.Uniq(append(adv.Aliases, ghsas...))
				adv.Aliases = updatedAliases

				u := v2.NewAdvisoriesSectionUpdater(func(doc v2.Document) (v2.Advisories, error) {
					// First, check if there would be a collision with the new CVE ID. If so, error
					// out.

					// Note: Until updated, the advisory in the document still has the original
					// ID (i.e. the GHSA ID).
					advisories := doc.Advisories.Update(ghsaFromAdvisoryID, adv)

					// Ensure the package's advisory list is sorted before returning it.
					sort.Sort(advisories)

					return advisories, nil
				})
				err = opts.AdvisoryDocs.Select().WhereName(doc.Name()).Update(u)
				if err != nil {
					return err
				}

				continue
			}
		}
	}

	return nil
}

// DuplicateAdvisoryIDError is returned when an attempt is made to add an
// advisory with an ID that already exists in the document.
type DuplicateAdvisoryIDError struct {
	// Package is the name of the package that already has an advisory with the same
	// ID.
	Package string

	// AdvisoryID is the ID of the advisory that already exists in the document.
	AdvisoryID string
}

func (e DuplicateAdvisoryIDError) Error() string {
	return fmt.Sprintf("package %q: rejecting duplicate advisory ID: %q", e.Package, e.AdvisoryID)
}

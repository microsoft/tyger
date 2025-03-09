// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Globalization;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Codespecs;

public class CodespecReader
{
    private readonly Repository _repository;

    public CodespecReader(Repository repository)
    {
        _repository = repository;
    }

    public async Task<Codespec> GetCodespec(ICodespecRef codespecRef, CancellationToken cancellationToken)
    {
        if (codespecRef is Codespec inlineCodespec)
        {
            return inlineCodespec;
        }

        if (codespecRef is not CommittedCodespecRef committedCodespecRef)
        {
            throw new InvalidOperationException("Invalid codespec reference");
        }

        if (committedCodespecRef.Version == null)
        {
            return await _repository.GetLatestCodespec(committedCodespecRef.Name, cancellationToken)
                ?? throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The codespec '{0}' was not found", committedCodespecRef.Name));
        }

        var codespec = await _repository.GetCodespecAtVersion(committedCodespecRef.Name, committedCodespecRef.Version.Value, cancellationToken);
        if (codespec == null)
        {
            // See if it's just the version number that was not found
            var latestCodespec = await _repository.GetLatestCodespec(committedCodespecRef.Name, cancellationToken)
                ?? throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The codespec '{0}' was not found", committedCodespecRef.Name));

            throw new ValidationException(
                string.Format(
                    CultureInfo.InvariantCulture,
                    "The version '{0}' of codespec '{1}' was not found. The latest version is '{2}'.",
                    committedCodespecRef.Version, committedCodespecRef.Name, latestCodespec.Version));
        }

        return codespec;
    }
}

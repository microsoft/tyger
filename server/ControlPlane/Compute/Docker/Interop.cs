// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Runtime.InteropServices;

namespace Tyger.ControlPlane.Compute.Docker;

internal static partial class Interop
{
    [LibraryImport("libSystem.Native", EntryPoint = "SystemNative_SymLink", SetLastError = true, StringMarshalling = StringMarshalling.Utf8)]
    internal static partial int SymLink(string target, string linkPath);

    [LibraryImport("libSystem.Native", EntryPoint = "SystemNative_MkFifo", SetLastError = true, StringMarshalling = StringMarshalling.Utf8)]
    internal static partial int MkFifo(string pathName, uint mode);

    [LibraryImport("libSystem.Native", EntryPoint = "SystemNative_ChMod", SetLastError = true, StringMarshalling = StringMarshalling.Utf8)]
    internal static partial int ChMod(string path, int mode);

    [LibraryImport("libSystem.Native", EntryPoint = "SystemNative_Stat", SetLastError = true, StringMarshalling = StringMarshalling.Utf8)]
    internal static partial int Stat(string path, out FileStatus output);

#pragma warning disable IDE1006 // Naming Styles

    [StructLayout(LayoutKind.Sequential)]
    internal struct FileStatus
    {
        public FileStatusFlags Flags;
        internal int Mode;
        internal uint Uid;
        internal uint Gid;
        internal long Size;
        internal long ATime;
        internal long ATimeNsec;
        internal long MTime;
        internal long MTimeNsec;
        internal long CTime;
        internal long CTimeNsec;
        internal long BirthTime;
        internal long BirthTimeNsec;
        internal long Dev;
        internal long RDev;
        internal long Ino;
        internal uint UserFlags;
    }

    internal static class FileTypes
    {
        internal const int S_IFMT = 0xF000;
        internal const int S_IFIFO = 0x1000;
        internal const int S_IFCHR = 0x2000;
        internal const int S_IFDIR = 0x4000;
        internal const int S_IFBLK = 0x6000;
        internal const int S_IFREG = 0x8000;
        internal const int S_IFLNK = 0xA000;
        internal const int S_IFSOCK = 0xC000;
    }

#pragma warning restore IDE1006 // Naming Styles

    [Flags]
    internal enum FileStatusFlags
    {
        None = 0,
        HasBirthTime = 1,
    }
}

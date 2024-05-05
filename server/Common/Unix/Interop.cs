// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Runtime.InteropServices;

namespace Tyger.Common.Unix;

public static partial class Interop
{
    [LibraryImport("libSystem.Native", EntryPoint = "SystemNative_SymLink", SetLastError = true, StringMarshalling = StringMarshalling.Utf8)]
    public static partial int SymLink(string target, string linkPath);

    [LibraryImport("libSystem.Native", EntryPoint = "SystemNative_MkFifo", SetLastError = true, StringMarshalling = StringMarshalling.Utf8)]
    public static partial int MkFifo(string pathName, uint mode);

    [LibraryImport("libSystem.Native", EntryPoint = "SystemNative_ChMod", SetLastError = true, StringMarshalling = StringMarshalling.Utf8)]
    public static partial int ChMod(string path, int mode);

    [LibraryImport("libSystem.Native", EntryPoint = "SystemNative_Stat", SetLastError = true, StringMarshalling = StringMarshalling.Utf8)]
    public static partial int Stat(string path, out FileStatus output);

#pragma warning disable IDE1006 // Naming Styles
#pragma warning disable CA1707 // Naming Styles

    [StructLayout(LayoutKind.Sequential)]
    public struct FileStatus
    {
        public int Flags;
        public int Mode;
        public uint Uid;
        public uint Gid;
        public long Size;
        public long ATime;
        public long ATimeNsec;
        public long MTime;
        public long MTimeNsec;
        public long CTime;
        public long CTimeNsec;
        public long BirthTime;
        public long BirthTimeNsec;
        public long Dev;
        public long RDev;
        public long Ino;
        public uint UserFlags;
    }

    public static class FileTypes
    {
        public const int S_IFMT = 0xF000;
        public const int S_IFIFO = 0x1000;
        public const int S_IFCHR = 0x2000;
        public const int S_IFDIR = 0x4000;
        public const int S_IFBLK = 0x6000;
        public const int S_IFREG = 0x8000;
        public const int S_IFLNK = 0xA000;
        public const int S_IFSOCK = 0xC000;
    }

#pragma warning restore IDE1006 // Naming Styles
#pragma warning restore CA1707 // Naming Styles
}

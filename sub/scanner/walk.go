package scanner

import (
	"crypto/sha512"
	"errors"
	"github.com/Symantec/Dominator/sub/fsrateio"
	"io"
	"os"
	"path"
	"sort"
	"syscall"
)

func (fileSystem *FileSystem) getInode(stat *syscall.Stat_t) (*Inode, bool) {
	inode := fileSystem.InodeTable[stat.Ino]
	new := false
	if inode == nil {
		var _inode Inode
		inode = &_inode
		_inode.Stat = *stat
		fileSystem.InodeTable[stat.Ino] = inode
		new = true
	}
	return inode, new
}

func (directory *Directory) scan(fileSystem *FileSystem,
	parentName string) error {
	myPathName := path.Join(parentName, directory.Name)
	file, err := os.Open(myPathName)
	if err != nil {
		return err
	}
	names, err := file.Readdirnames(-1)
	if err != nil {
		return err
	}
	file.Close()
	sort.Strings(names)
	for _, name := range names {
		filename := path.Join(myPathName, name)
		var stat syscall.Stat_t
		err := syscall.Lstat(filename, &stat)
		if err != nil {
			if err == syscall.ENOENT {
				continue
			}
			return err
		}
		inode, isNewInode := fileSystem.getInode(&stat)
		if stat.Dev == directory.Inode.Stat.Dev {
			if stat.Mode&syscall.S_IFMT == syscall.S_IFDIR {
				if !isNewInode {
					return errors.New("Hardlinked directory: " + filename)
				}
				var dir Directory
				dir.Name = name
				dir.Inode = inode
				err := dir.scan(fileSystem, myPathName)
				if err != nil {
					return err
				}
				directory.DirectoryList = append(directory.DirectoryList, &dir)
			} else {
				var file File
				file.Name = name
				file.Inode = inode
				if isNewInode {
					err := file.scan(fileSystem, myPathName)
					if err != nil {
						if err == syscall.ENOENT {
							continue
						}
						return err
					}
				}
				directory.FileList = append(directory.FileList, &file)
			}
		}
	}
	return nil
}

func (file *File) scan(fileSystem *FileSystem, parentName string) error {
	myPathName := path.Join(parentName, file.Name)
	if file.Inode.Stat.Mode&syscall.S_IFMT == syscall.S_IFREG {
		f, err := os.Open(myPathName)
		if err != nil {
			return err
		}
		reader := fsrateio.NewReader(f, fileSystem.ctx)
		hash := sha512.New()
		io.Copy(hash, reader)
		f.Close()
		file.Inode.Hash = hash.Sum(nil)
	} else if file.Inode.Stat.Mode&syscall.S_IFMT == syscall.S_IFLNK {
		symlink, err := os.Readlink(myPathName)
		if err != nil {
			return err
		}
		file.Inode.Symlink = symlink
	}
	return nil
}

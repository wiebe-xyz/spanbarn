package service

import "github.com/wiebe-xyz/spanbarn/internal/repository"

// UserRepository is the storage UserService needs.
type UserRepository interface {
	CreateUser(username, passwordHash string) error
	DeleteUser(username string) error
	ListUsers() ([]repository.User, error)
}

// UserService manages login accounts for the CLI. Errors pass through
// unchanged.
type UserService struct{ repo UserRepository }

func NewUserService(repo UserRepository) *UserService { return &UserService{repo: repo} }

func (s *UserService) Create(username, passwordHash string) error {
	return s.repo.CreateUser(username, passwordHash)
}
func (s *UserService) Delete(username string) error     { return s.repo.DeleteUser(username) }
func (s *UserService) List() ([]repository.User, error) { return s.repo.ListUsers() }

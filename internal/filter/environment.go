package filter

import "regexp"

// EnvironmentKey is the attribute that carries a record's environment.
const EnvironmentKey = "deployment.environment"

// ProductionEnvironment is the environment that also covers records which
// carry none: a sender that never set one has always been production.
const ProductionEnvironment = "production"

// EnvironmentNode is the condition that selects one environment. production
// also selects spans without the attribute. It returns false for "" and "all",
// which select every environment.
func EnvironmentNode(env string) (Node, bool) {
	switch env {
	case "", "all":
		return Node{}, false
	case ProductionEnvironment:
		return Node{Match: MatchOr, Filters: []Node{
			{Key: EnvironmentKey, Op: OpEq, Value: Scalar(env)},
			{Key: EnvironmentKey, Op: OpNotExists},
		}}, true
	}
	return Node{Key: EnvironmentKey, Op: OpEq, Value: Scalar(env)}, true
}

var environmentName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidEnvironmentName reports whether s is a name an environment may have: a
// lowercase letter or digit, then lowercase letters, digits, dots, dashes and
// underscores, 64 characters at most.
func ValidEnvironmentName(s string) bool { return environmentName.MatchString(s) }

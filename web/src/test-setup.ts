import '@testing-library/jest-dom'
import { configure } from '@testing-library/react'

// The shared CI runner is also the build farm for other repos and regularly
// runs at several times its core count. findBy*/waitFor resolve as soon as the
// awaited transition happens, so a larger ceiling costs nothing when the host
// is idle and only stops slow renders from failing on the 1s default.
configure({ asyncUtilTimeout: 10_000 })

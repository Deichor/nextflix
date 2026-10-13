pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "nextflix-tv"

include(
    ":app",
    ":core",
    ":feature:player",
    ":feature:subtitles",
    ":feature:browse",
    ":feature:detail",
    ":feature:search",
    ":feature:account",
)

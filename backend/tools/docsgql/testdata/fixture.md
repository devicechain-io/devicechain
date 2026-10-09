# Fixture

## valid

```graphql
query {
  things(criteria: {pageNumber: 1, pageSize: 5}) { token name }
}
```

## valid with attribute-style info string

```graphql{1}
query { thingsByToken(tokens: ["a"]) { token } }
```

## unknown field

```graphql
query { thingsByToken(tokens: ["a"]) { token noSuchField } }
```

## missing variable decl

```graphql
query { thingsByToken(tokens: $t) { token } }
```

## wrong argument type

```graphql
query { things(criteria: {pageNumber: "one", pageSize: 5}) { token } }
```

## unknown area

```graphql
# schema: nowhere
query { thingsByToken(tokens: ["a"]) { token } }
```

## subscription without a root

```graphql
subscription { noSuchStream(zzz: 1) { qqq } }
```

## capitalised keyword

```graphql
Query { thingsByToken(tokens: ["a"]) { nope } }
```

## misspelt keyword

```graphql
qeury { thingsByToken(tokens: ["a"]) { nope } }
```

## bare selection

```graphql
thingsByToken(tokens: ["a"]) { nope }
```

## description lead

```graphql
"""doc"""
query { thingsByToken(tokens: ["a"]) { nope } }
```

## empty block

```graphql
# only a comment
```

## sdl then operation

```graphql
type Thing { token: String! }
query { thingsByToken(tokens: ["a"]) { nope } }
```

## sdl is skipped

```graphql
type Thing { token: String! }
```

## signature is skipped

```graphql
cancelThing(token: String!): Thing!
```

```json
{ "not": "graphql" }
```
